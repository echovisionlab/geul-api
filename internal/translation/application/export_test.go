package application

import "time"

func (s *Service) SetClockForTest(now func() time.Time) { s.now = now }
