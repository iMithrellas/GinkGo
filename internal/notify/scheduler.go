package notify

import "time"

// Scheduler is a simple placeholder for notification scheduling.
type Scheduler struct {
	Every time.Duration
}

func (s *Scheduler) Next(now time.Time) time.Time {
	if s.Every <= 0 {
		return time.Time{}
	}
	return now.Add(s.Every)
}
