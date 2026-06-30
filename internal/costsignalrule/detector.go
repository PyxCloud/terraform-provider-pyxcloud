package costsignalrule

// Detector evaluates a set of CostBlowoutRules against cost data and returns
// any triggered signals.  It implements the billing signal gate described in
// the package documentation.
type Detector struct {
	rules []CostBlowoutRule
}

// NewDetector creates a Detector with the given rule set.
func NewDetector(rules []CostBlowoutRule) *Detector {
	return &Detector{rules: rules}
}

// DefaultRules returns a copy of the three built-in cost-blowout rules.
func DefaultRules() []CostBlowoutRule {
	return []CostBlowoutRule{
		DefaultBudgetOverrunRule,
		DefaultCostSpikeRule,
		DefaultResourceCostAnomalyRule,
	}
}

// EvaluateAll runs every rule in the detector against costData and baseline.
// costData is a map of resource type -> current cost.
// baseline is a map of resource type -> expected / previous-window cost.
// Returns all signals that fired; an empty slice means no blowout detected.
func (d *Detector) EvaluateAll(costData map[string]float64, baseline map[string]float64) []*CostBlowoutSignal {
	var signals []*CostBlowoutSignal
	for i := range d.rules {
		if sig := d.rules[i].Evaluate(costData, baseline); sig != nil {
			signals = append(signals, sig)
		}
	}
	return signals
}

// AddRule appends a rule to the detector's active rule set.
func (d *Detector) AddRule(rule CostBlowoutRule) {
	d.rules = append(d.rules, rule)
}

// RuleCount returns the number of rules currently registered in the detector.
func (d *Detector) RuleCount() int {
	return len(d.rules)
}
