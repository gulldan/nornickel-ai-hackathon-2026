package application

import "strings"

const (
	researchTagTheoretical = "теоретическое исследование"
	researchTagPractical   = "практическое"
)

func normalizeResearchTag(s string) string {
	v := strings.ToLower(strings.TrimSpace(s))
	switch {
	case strings.Contains(v, "теорет"):
		return researchTagTheoretical
	case strings.Contains(v, "практ"):
		return researchTagPractical
	default:
		return ""
	}
}

func inferResearchTag(measurable bool, parts ...string) string {
	text := strings.ToLower(strings.Join(parts, " "))
	for _, marker := range []string{
		"внедрен", "производств", "промышлен", "эксплуатац", "пилот",
		"натурн", "эффект", "снижен", "снижени", "повыш", "увелич", "испытан",
	} {
		if strings.Contains(text, marker) {
			return researchTagPractical
		}
	}
	for _, marker := range []string{
		"модель", "модел", "расчет", "расчёт", "симуля", "теорет",
		"алгоритм", "уравнен", "численн", "методик", "методолог",
	} {
		if strings.Contains(text, marker) {
			return researchTagTheoretical
		}
	}
	if measurable {
		return researchTagPractical
	}
	return researchTagTheoretical
}

func withResearchTag(tags []string, research string) []string {
	research = normalizeResearchTag(research)
	if research == "" {
		return dedupeTags(tags)
	}
	out := make([]string, 0, len(tags)+1)
	out = append(out, research)
	for _, tag := range tags {
		v := strings.TrimSpace(tag)
		if v == "" || normalizeResearchTag(v) != "" {
			continue
		}
		out = append(out, v)
	}
	return dedupeTags(out)
}

func mergeTags(groups ...[]string) []string {
	total := 0
	for _, group := range groups {
		total += len(group)
	}
	out := make([]string, 0, total)
	for _, group := range groups {
		out = append(out, group...)
	}
	return dedupeTags(out)
}

func dedupeTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, tag := range tags {
		v := strings.TrimSpace(tag)
		if v == "" {
			continue
		}
		key := strings.ToLower(v)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, v)
	}
	return out
}
