package service

// USTCGenerationTPS uses the common usage-record definition: output tokens
// divided by time after first output, including the response tail. It is an
// observed average, not an event-to-event token latency estimate.
func USTCGenerationTPS(obs USTCSpeedObservation, streaming bool) *float64 {
	if !streaming || !obs.TokenCountKnown || obs.OutputTokens < 2 || obs.TotalMS == nil || obs.FirstOutputMS == nil || *obs.FirstOutputMS < 0 || *obs.TotalMS <= *obs.FirstOutputMS {
		return nil
	}
	value := float64(obs.OutputTokens) * 1000 / (*obs.TotalMS - *obs.FirstOutputMS)
	if !isFinitePositive(value) {
		return nil
	}
	return &value
}
