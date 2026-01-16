package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type RateLimiter struct {
	store  map[string]*[]int64
	mu     sync.Mutex
	expire time.Duration
}

func (l *RateLimiter) Init(expire time.Duration) {
	if l.store == nil {
		l.mu.Lock()
		if l.store == nil {
			l.store = make(map[string]*[]int64)
			l.expire = expire
			if expire > 0 {
				go l.clearExpiredItems()
			}
		}
		l.mu.Unlock()
	}
}

func (l *RateLimiter) clearExpiredItems() {
	for {
		time.Sleep(l.expire)
		l.mu.Lock()
		now := time.Now().Unix()
		for key := range l.store {
			queue := l.store[key]
			size := len(*queue)
			if size == 0 || now-(*queue)[size-1] > int64(l.expire.Seconds()) {
				delete(l.store, key)
			}
		}
		l.mu.Unlock()
	}
}

// Request parameter duration's unit is seconds
func (l *RateLimiter) Request(key string, maxRequestNum int, duration int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	// [old <-- new]
	queue, ok := l.store[key]
	now := time.Now().Unix()
	if ok {
		if len(*queue) < maxRequestNum {
			*queue = append(*queue, now)
			return true
		} else {
			if now-(*queue)[0] >= duration {
				*queue = (*queue)[1:]
				*queue = append(*queue, now)
				return true
			} else {
				return false
			}
		}
	} else {
		s := make([]int64, 0, maxRequestNum)
		l.store[key] = &s
		*(l.store[key]) = append(*(l.store[key]), now)
	}
	return true
}

func IpRateLimiter(maxRequestNum int, duration int64) func(c *gin.Context) {
	rl := &RateLimiter{}
	rl.Init(time.Duration(duration) * time.Second)

	return func(c *gin.Context) {
		ip := "" + c.ClientIP()
		if !rl.Request(ip, maxRequestNum, duration) {
			c.AbortWithStatus(http.StatusTooManyRequests)
		}
		c.Next()
	}
}
