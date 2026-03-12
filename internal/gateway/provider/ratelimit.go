package provider

import (
	"sync"
	"time"
)

type RateLimiter struct {
	maxRequestsPerMinute int
	maxConcurrent        int
	
	requests map[string]int
	mu       sync.RWMutex
}

func NewRateLimiter(maxRequestsPerMinute, maxConcurrent int) *RateLimiter {
	return &RateLimiter{
		maxRequestsPerMinute: maxRequestsPerMinute,
		maxConcurrent:        maxConcurrent,
		requests:             make(map[string]int),
	}
}

func (rl *RateLimiter) Allow(agentID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	
	rl.requests[agentID]++
	
	if rl.requests[agentID] > rl.maxRequestsPerMinute {
		return false
	}
	
	go func() {
		time.Sleep(1 * time.Minute)
		rl.mu.Lock()
		delete(rl.requests, agentID)
		rl.mu.Unlock()
	}()
	
	return true
}
