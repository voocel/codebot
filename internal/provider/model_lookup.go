package provider

import "strings"

// SameModelID reports whether two model IDs refer to the same canonical model.
func SameModelID(a, b string) bool {
	return modelLookupMatches(normalizeModelLookupID(a), normalizeModelLookupID(b))
}

func lookupGeneratedModel(providerName, modelID string) (ModelEntry, bool) {
	return lookupModelEntry(generatedModels, providerName, modelID)
}

func lookupModelEntry(models []ModelEntry, providerName, modelID string) (ModelEntry, bool) {
	providerName = strings.ToLower(strings.TrimSpace(providerName))
	targetID := normalizeModelLookupID(modelID)
	for _, model := range models {
		if providerName != "" && !strings.EqualFold(model.Provider, providerName) {
			continue
		}
		if modelLookupMatches(normalizeModelLookupID(model.ID), targetID) {
			return model, true
		}
	}
	return ModelEntry{}, false
}

func normalizeModelLookupID(modelID string) string {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	return strings.ReplaceAll(modelID, ".", "-")
}

func modelLookupMatches(knownID, targetID string) bool {
	if knownID == targetID {
		return true
	}
	if strings.HasPrefix(targetID, knownID) && isDatedModelSuffix(targetID[len(knownID):]) {
		return true
	}
	if strings.HasPrefix(knownID, targetID) && isDatedModelSuffix(knownID[len(targetID):]) {
		return true
	}
	return false
}

func isDatedModelSuffix(s string) bool {
	if len(s) != 9 || s[0] != '-' {
		return false
	}
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
