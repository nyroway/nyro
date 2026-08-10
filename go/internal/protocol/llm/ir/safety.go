package ir

// SafetySettings is a Google GenAI safety setting — important enough to have a
// first-class home in the IR (ignored by non-Google encoders).
type SafetySettings struct {
	Category  string
	Threshold string
}
