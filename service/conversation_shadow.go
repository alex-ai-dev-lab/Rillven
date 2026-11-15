package service

// Shadow observes only self-contained turns. It never changes protocol payloads,
// recovery eligibility, or availability when the observation backend fails.
type shadowConversation struct {
	*conversationAttempt
	failed   bool
	quota    int
	ledgerID uint64
}

func (s *shadowConversation) ObservationOnly() bool { return true }

func (s *shadowConversation) ObserveResponse(payload []byte, stream bool) ([]byte, error) {
	if !s.failed {
		if _, err := s.conversationAttempt.ObserveResponse(payload, stream); err != nil {
			s.failed = true
		}
	}
	return payload, nil
}
func (s *shadowConversation) ValidateResponse() error { return nil }
func (s *shadowConversation) PrepareCommit(quota int, ledgerID uint64) error {
	s.quota, s.ledgerID = quota, ledgerID
	return nil
}
func (s *shadowConversation) PublishCommit() error {
	if !s.failed {
		if err := s.conversationAttempt.PrepareCommit(s.quota, s.ledgerID); err == nil {
			_ = s.conversationAttempt.PublishCommit()
		}
	}
	return nil
}
func (s *shadowConversation) CommitPrepared() bool  { return false }
func (s *shadowConversation) CommitPublished() bool { return true }
