package pubsearch

import "strings"

type termDomain int

const (
	domainNeutral termDomain = iota
	domainBeneficiation
	domainMetallurgy
)

const (
	maxTerms = 8
	minTerms = 2
)

type dictEntry struct {
	rus []string
	en  string
	dom termDomain
}

func dictionary() []dictEntry {
	return []dictEntry{
		{rus: []string{"флотац"}, en: "flotation", dom: domainBeneficiation},
		{rus: []string{"хвост"}, en: "tailings", dom: domainBeneficiation},
		{rus: []string{"извлечен"}, en: "recovery", dom: domainNeutral},
		{rus: []string{"потер"}, en: "losses", dom: domainNeutral},
		{rus: []string{"медь", "меди"}, en: "copper", dom: domainNeutral},
		{rus: []string{"никел"}, en: "nickel", dom: domainNeutral},
		{rus: []string{"золот"}, en: "gold", dom: domainNeutral},
		{rus: []string{"доизмельчен"}, en: "regrinding", dom: domainBeneficiation},
		{rus: []string{"измельчен"}, en: "grinding", dom: domainBeneficiation},
		{rus: []string{"классификац"}, en: "classification", dom: domainBeneficiation},
		{rus: []string{"гидроциклон"}, en: "hydrocyclone", dom: domainBeneficiation},
		{rus: []string{"реагент"}, en: "reagent", dom: domainBeneficiation},
		{rus: []string{"собират"}, en: "collector", dom: domainBeneficiation},
		{rus: []string{"депрессор"}, en: "depressant", dom: domainBeneficiation},
		{rus: []string{"пенообразоват"}, en: "frother", dom: domainBeneficiation},
		{rus: []string{"шлам"}, en: "slimes", dom: domainBeneficiation},
		{rus: []string{"сгущен"}, en: "thickening", dom: domainBeneficiation},
		{rus: []string{"аэрац"}, en: "aeration", dom: domainBeneficiation},
		{rus: []string{"пульп"}, en: "pulp density", dom: domainBeneficiation},
		{rus: []string{"концентрат"}, en: "concentrate", dom: domainBeneficiation},
		{rus: []string{"шлак"}, en: "slag", dom: domainMetallurgy},
		{rus: []string{"плавк"}, en: "smelting", dom: domainMetallurgy},
		{rus: []string{"выщелачиван"}, en: "leaching", dom: domainMetallurgy},
		{rus: []string{"обжиг"}, en: "roasting", dom: domainMetallurgy},
		{rus: []string{"электролиз"}, en: "electrowinning", dom: domainMetallurgy},
		{rus: []string{"штейн"}, en: "matte", dom: domainMetallurgy},
		{rus: []string{"флюс"}, en: "flux", dom: domainMetallurgy},
		{rus: []string{"футеровк"}, en: "mill liner", dom: domainBeneficiation},
		{rus: []string{"мельниц"}, en: "mill", dom: domainBeneficiation},
		{rus: []string{"дроблен"}, en: "crushing", dom: domainBeneficiation},
		{rus: []string{"грохочен"}, en: "screening", dom: domainBeneficiation},
		{rus: []string{"сросток", "сростк"}, en: "locked particles", dom: domainBeneficiation},
		{rus: []string{"раскрыт"}, en: "liberation", dom: domainBeneficiation},
		{rus: []string{"крупн"}, en: "coarse fraction", dom: domainBeneficiation},
		{rus: []string{"тонк"}, en: "fine fraction", dom: domainBeneficiation},
		{rus: []string{"руд"}, en: "ore", dom: domainBeneficiation},
		{rus: []string{"обогащен"}, en: "beneficiation", dom: domainBeneficiation},
		{rus: []string{"сепарац"}, en: "separation", dom: domainBeneficiation},
		{rus: []string{"магнитн"}, en: "magnetic", dom: domainBeneficiation},
	}
}

// QueryFromRu builds an English search query from a Russian hypothesis goal
// and constraints using a mineral-processing/metallurgy dictionary. Latin
// words longer than two characters pass through as-is. It returns an empty
// string when fewer than two terms are found.
func QueryFromRu(goal, constraints string) string {
	text := strings.ToLower(goal + " " + constraints)
	seen := map[string]bool{}
	terms := make([]string, 0, maxTerms)
	benef, metall := 0, 0
	for _, e := range dictionary() {
		if seen[e.en] || !containsAny(text, e.rus) {
			continue
		}
		seen[e.en] = true
		terms = append(terms, e.en)
		switch e.dom {
		case domainBeneficiation:
			benef++
		case domainMetallurgy:
			metall++
		case domainNeutral:
		}
	}
	for _, w := range latinWords(text) {
		if seen[w] {
			continue
		}
		seen[w] = true
		terms = append(terms, w)
	}
	if len(terms) < minTerms {
		return ""
	}
	if len(terms) > maxTerms {
		terms = terms[:maxTerms]
	}
	switch {
	case benef >= minTerms:
		terms = append(terms, "mineral processing")
	case metall >= minTerms:
		terms = append(terms, "extractive metallurgy")
	}
	return strings.Join(terms, " ")
}

func containsAny(text string, subs []string) bool {
	for _, s := range subs {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}

func latinWords(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r < 'a' || r > 'z'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) > 2 {
			out = append(out, f)
		}
	}
	return out
}
