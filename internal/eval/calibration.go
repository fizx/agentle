package eval

// LabelPair is one (human, judge) agreement observation. true = success/pass.
type LabelPair struct {
	Human bool
	Judge bool
}

// CalibrationStats measures judge↔human agreement over a labeled corpus — the
// gate that must clear before verdicts are trusted. Accuracy alone is misleading
// when labels are imbalanced, so Cohen's kappa (chance-corrected agreement) is
// reported alongside it.
type CalibrationStats struct {
	N          int     `json:"n"`
	Agreements int     `json:"agreements"`
	Accuracy   float64 `json:"accuracy"`
	Kappa      float64 `json:"kappa"`
	// Confusion counts, treating success/pass as the positive class.
	TP int `json:"tp"` // human pass, judge pass
	TN int `json:"tn"` // human fail, judge fail
	FP int `json:"fp"` // human fail, judge pass (judge too lenient)
	FN int `json:"fn"` // human pass, judge fail (judge too strict)
}

// Calibrate computes agreement stats for the corpus. Kappa is 0 when there is no
// variance to correct for (e.g. all-agree-by-chance), following the convention
// that perfectly-expected agreement carries no information.
func Calibrate(pairs []LabelPair) CalibrationStats {
	st := CalibrationStats{N: len(pairs)}
	if st.N == 0 {
		return st
	}
	for _, p := range pairs {
		switch {
		case p.Human && p.Judge:
			st.TP++
		case !p.Human && !p.Judge:
			st.TN++
		case !p.Human && p.Judge:
			st.FP++
		default:
			st.FN++
		}
	}
	st.Agreements = st.TP + st.TN
	n := float64(st.N)
	po := float64(st.Agreements) / n // observed agreement (= accuracy)
	st.Accuracy = po

	// Expected agreement by chance, from the marginal rates of each rater.
	humanPos := float64(st.TP+st.FN) / n
	judgePos := float64(st.TP+st.FP) / n
	pe := humanPos*judgePos + (1-humanPos)*(1-judgePos)
	if pe >= 1.0 {
		st.Kappa = 0 // no room above chance; report no signal
	} else {
		st.Kappa = (po - pe) / (1 - pe)
	}
	return st
}
