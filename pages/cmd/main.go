package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path"
	"text/template"
	"time"

	"engine/core/lyrics"
	"engine/history"
	"engine/pages/limiter"

	"github.com/go-chi/chi"
	"github.com/go-chi/chi/middleware"
	"github.com/jessevdk/go-flags"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
)

var argv struct {
	AppAddr     string     `long:"addr" env:"PAGES_APP_ADDR" description:"address to start application at"`
	MetricsAddr string     `long:"metrics-addr" env:"PAGES_METRICS_ADDR" description:"address to expose metrics at"`
	ClickDBName string     `long:"click-db" default:"elvis"`
	ClickDSN    string     `long:"click-dsn" env:"PAGES_CLICKHOUSE_DSN" description:"clickhouse dsn string to connect"`
	RPSLimit    rate.Limit `long:"rate" env:"PAGES_IP_RPS_LIMIT" description:"maximum RPS per IP" default:"10"`
	BurstLimit  int        `long:"burst" env:"PAGES_IP_BURST_LIMIT" description:"maximum amount of available request burst from one IP" default:"50"`
	TemplateDir string     `long:"template-dir" description:"directory which contains proper templates to serve"`

	ThrRate           int           `long:"trate" default:"1000" description:"Throttle rate of incoming requests"`
	ThrBacklogLimit   int           `long:"tbacklimit" default:"1000" description:"Throttle limit of pending requests"`
	ThrBacklogTimeout time.Duration `long:"tbacktime" default:"30s" description:"Throttle lifetime of pending requests"`
	SleepTimeMin      time.Duration `long:"sleepTimeMin" description:"Limiter clean up sleep time"`
	LifeTimeMin       time.Duration `long:"lifeTimeMin" description:"Limiter users lifetime"`

	Debug bool `long:"debug" description:"makes application log more verbose"`
}

func init() {
	if _, err := flags.Parse(&argv); err != nil {
		os.Exit(1)
	}
}

func main() {
	log, _ := zap.NewProduction()
	if argv.Debug {
		log, _ = zap.NewDevelopment()
	}
	defer log.Sync()
	log.Sugar().Infof("init: %+v", argv)

	app, err := NewApp(log)
	if err != nil {
		log.Sugar().Error(err)
	}
	defer log.Sugar().Error(app.history.Close())
	app.Listen()
}

type App struct {
	history history.Engine
	lyrics  lyrics.Engine
	metrics pageMetrics

	pageLimiter *limiter.Limiter
	pagesSrv    *http.Server
	metricsSrv  *http.Server
	log         *zap.Logger
}

type pageMetrics struct {
	pagesUsed prometheus.Gauge
}

// NewApp creates new app with logger. Sets 'actor' equals to 'history_engine'
func NewApp(withLogger *zap.Logger) (*App, error) {
	dsn := argv.ClickDSN
	click, err := history.InitClickHouse(dsn,
		withLogger.With(zap.String("actor", "history_engine")))
	if err != nil {
		withLogger.Fatal("history engine is unreachable", zap.Error(err))
	}

	app := &App{
		log:         withLogger,
		pageLimiter: limiter.InitLimiter(argv.SleepTimeMin, argv.LifeTimeMin, argv.RPSLimit, argv.BurstLimit),
		history:     click,
		lyrics:      lyrics.NewEngine(withLogger.With(zap.String("actor", "lyrics_engine"))),
	}
	app.metricsSrv = app.initMetrics()
	app.pagesSrv = app.initPagesServer()

	return app, nil
}

func (a *App) initPagesServer() *http.Server {
	router := chi.NewRouter()

	// Sets a http.Request's RemoteAddr to either X-Forwarded-For or X-Real-IP
	router.Use(middleware.RealIP)
	router.Use(middleware.ThrottleBacklog(argv.ThrRate, argv.ThrBacklogLimit, argv.ThrBacklogTimeout))

	router.HandleFunc("/recognition/{id}", a.serveRecognitionPage)
	router.HandleFunc("/lyrics/{id}", a.serveLyrics)
	srv := &http.Server{
		Handler:      router,
		Addr:         argv.AppAddr,
		WriteTimeout: 10 * time.Second,
		ReadTimeout:  10 * time.Second,
	}
	return srv
}

func (a *App) initMetrics() *http.Server {
	subsystem := "pages"
	a.metrics.pagesUsed = prometheus.NewGauge(prometheus.GaugeOpts{
		Subsystem: subsystem,
		Name:      "total_pages_calls",
		Help:      "Total amount of pages calls",
	})
	prometheus.MustRegister(a.metrics.pagesUsed)

	metrics := chi.NewRouter()
	metrics.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{
		Handler:      metrics,
		Addr:         argv.MetricsAddr,
		WriteTimeout: 10 * time.Second,
		ReadTimeout:  10 * time.Second,
	}

	return srv
}

func (a *App) Listen() {
	go a.metricsSrv.ListenAndServe()

	a.log.Sugar().Infof("metrics listening on %v", a.metricsSrv.Addr)
	a.log.Sugar().Infof("pages listening on %v", a.pagesSrv.Addr)

	a.log.Sugar().Error(a.pagesSrv.ListenAndServe())
}

func (a *App) serveRecognitionPage(w http.ResponseWriter, r *http.Request) {
	// Check visitor's request limit
	err := a.checkLimits(r.RemoteAddr)
	if err != nil {
		a.respondWithError(w, fmt.Errorf("sorry, your recognition limit has exceeded"), http.StatusNoContent)
		return
	}
	go a.metrics.pagesUsed.Inc()

	tmpl, err := template.ParseFiles(path.Join(argv.TemplateDir, "index.html"))
	if err != nil {
		a.respondWithError(w, fmt.Errorf("oops, internal error, be right back"), http.StatusInternalServerError)
		panic(err)
	}

	recognitionID := chi.URLParam(r, "id")
	recogs, err := a.history.Load(recognitionID)
	if err != nil {
		a.respondWithError(w, fmt.Errorf("can't load recognition: %w", err), http.StatusInternalServerError)
		return
	}

	tmpl.Execute(w, recogs)
}

func (a *App) serveLyrics(w http.ResponseWriter, r *http.Request) {
	// Check visitor's request limit
	err := a.checkLimits(r.RemoteAddr)
	if err != nil {
		a.respondWithError(w, fmt.Errorf("sorry, your recognition limit has exceeded"), http.StatusNoContent)
		return
	}
	go a.metrics.pagesUsed.Inc()

	recognitionID := chi.URLParam(r, "id")

	// find corresponding artist and title
	recognition, err := a.history.Load(recognitionID)
	if err != nil {
		a.respondWithError(w, fmt.Errorf("can't load recognition: %w", err), http.StatusInternalServerError)
		return
	}
	if len(recognition) < 1 {
		a.respondWithError(w, fmt.Errorf("empty recognition: %w", err), http.StatusInternalServerError)
		return
	}
	// find lyrics
	lyrics, err := a.lyrics.Lookup(recognition[0].Artist, recognition[0].Title)
	if err != nil {
		a.respondWithError(w, fmt.Errorf("can't load recognition: %w", err), http.StatusInternalServerError)
		return
	}

	response, err := json.Marshal(lyrics.Text)
	if err != nil {
		a.respondWithError(w, fmt.Errorf("Error while marshaling: %w", err), http.StatusInternalServerError)
		return
	}

	_, err = w.Write(response)
	if err != nil {
		a.respondWithError(w, fmt.Errorf("error while writing response: %w", err), http.StatusInternalServerError)
		return
	}
}

func (a *App) respondWithError(w http.ResponseWriter, err error, code int) {
	a.log.Error("", zap.Int("code", code), zap.Error(err))
	w.WriteHeader(code)
	fmt.Fprintf(w, "%s\n", err.Error())
}

func (a *App) checkLimits(RemoteAddr string) error {
	// Get visitor's ip
	ip, _, err := net.SplitHostPort(RemoteAddr)
	if err != nil {
		return fmt.Errorf("split IP error: %v", err)
	}
	// Check visitor's request limit
	limit := a.pageLimiter.GetVisitor(ip)
	if limit.Allow() == false {
		return fmt.Errorf("too many requests")
	}
	return nil
}
