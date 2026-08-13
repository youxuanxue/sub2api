package service

import (
	"sort"
	"strings"
)

// resolveWildcardMappingTarget returns the upstream model id for a wildcard rule.
// Identity rules (pattern == target, or target is a matching wildcard) preserve
// the client's requested spelling — needed for CloudWise floors like "minimax-*".
func resolveWildcardMappingTarget(pattern, target, requestedModel string) string {
	if target == pattern {
		return requestedModel
	}
	if strings.HasSuffix(target, "*") && matchWildcard(target, requestedModel) {
		return requestedModel
	}
	return target
}

func matchWildcardMappingResult(mapping map[string]string, requestedModel string) (string, bool) {
	type patternMatch struct {
		pattern string
		target  string
	}
	var matches []patternMatch

	for pattern, target := range mapping {
		if matchWildcard(pattern, requestedModel) {
			matches = append(matches, patternMatch{pattern, target})
		}
	}

	if len(matches) == 0 {
		return requestedModel, false
	}

	sort.Slice(matches, func(i, j int) bool {
		if len(matches[i].pattern) != len(matches[j].pattern) {
			return len(matches[i].pattern) > len(matches[j].pattern)
		}
		return matches[i].pattern < matches[j].pattern
	})

	return resolveWildcardMappingTarget(matches[0].pattern, matches[0].target, requestedModel), true
}

func matchNormalizedWildcardMappingResult(mapping map[string]string, lookupKey, originalRequestedModel string) (string, bool) {
	type patternMatch struct {
		pattern string
		target  string
	}
	var matches []patternMatch
	for pattern, target := range mapping {
		normalizedPattern := normalizeModelMappingLookupKey(pattern)
		if matchWildcard(normalizedPattern, lookupKey) {
			matches = append(matches, patternMatch{pattern: pattern, target: target})
		}
	}
	if len(matches) == 0 {
		return originalRequestedModel, false
	}
	sort.Slice(matches, func(i, j int) bool {
		if len(matches[i].pattern) != len(matches[j].pattern) {
			return len(matches[i].pattern) > len(matches[j].pattern)
		}
		return matches[i].pattern < matches[j].pattern
	})
	return resolveWildcardMappingTarget(matches[0].pattern, matches[0].target, originalRequestedModel), true
}
