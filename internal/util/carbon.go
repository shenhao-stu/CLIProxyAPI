package util

import "strings"

func IsGeminiCarbon(model string) bool {
	switch strings.ToLower(model) {
	case "gemini-4-carbon", "gemini-4-carbon-low", "gemini-4-carbon-medium", "gemini-4-carbon-high":
		return true
	}
	return false
}
