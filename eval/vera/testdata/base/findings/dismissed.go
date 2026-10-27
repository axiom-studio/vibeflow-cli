package findings

// Suppress drops the findings a person dismissed in any earlier review of the
// repository, so a later review never raises a dismissed defect again.
func (s *Store) Suppress(found []Finding) []Finding {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := found[:0]
	for _, f := range found {
		if _, dismissed := s.dismissals[f.Fingerprint]; dismissed {
			continue
		}
		kept = append(kept, f)
	}
	return kept
}
