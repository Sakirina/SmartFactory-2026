package model

// NodeKinds is shared by the definition validator and its published contract.
// Each call returns its own map so clients cannot modify global validation rules.
func NodeKinds() map[string][]string {
	return map[string][]string{
		"input":      {"analysis", "alarm", "strategy"},
		"aggregate":  {"analysis", "alarm"},
		"expression": {"analysis", "alarm", "strategy"},
		"threshold":  {"alarm", "strategy"},
		"hysteresis": {"alarm", "strategy"},
		"debounce":   {"alarm", "strategy"},
		"counter":    {"analysis"}, "output": {"analysis"}, "alarm": {"alarm"},
		"condition": {"strategy"}, "action": {"strategy"},
		"branch": {"analysis", "alarm", "strategy"},
	}
}
