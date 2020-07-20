package limiter

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

//Limiter is an limiter struct
type Limiter struct {
	sync.RWMutex
	list         map[string]*Visitor
	sleepTimeMin time.Duration
	lifeTimeMin  time.Duration
	rate         rate.Limit
	burst        int
}

//Visitor - single visitor struct
type Visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

//InitLimiter creates new limiter
func InitLimiter(sleepTimeMin, lifeTimeMin time.Duration, rate rate.Limit, burst int) *Limiter {
	return &Limiter{
		list:         make(map[string]*Visitor),
		sleepTimeMin: sleepTimeMin,
		lifeTimeMin:  lifeTimeMin,
		rate:         rate,
		burst:        burst,
	}
}

//GetVisitor check visitors limits
func (lc *Limiter) GetVisitor(ip string) *rate.Limiter {
	lc.Lock()
	defer lc.Unlock()

	v, exists := lc.list[ip]
	if !exists {

		limiterPage := rate.NewLimiter(lc.rate, lc.burst)
		// Include the current time when creating a new visitor.
		lc.list[ip] = &Visitor{limiterPage, time.Now()}

		return limiterPage
	}
	// Update the last seen time for the visitor.
	v.lastSeen = time.Now()
	return v.limiter
}

//CleanupVisitors cleans list of visitors every n minutes
func (lc *Limiter) CleanupVisitors() {
	for {
		time.Sleep(lc.sleepTimeMin)
		lc.Lock()
		for ip, v := range lc.list {
			if time.Since(v.lastSeen) > lc.lifeTimeMin {
				delete(lc.list, ip)
			}
		}
		lc.Unlock()
	}
}
