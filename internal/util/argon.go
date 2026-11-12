package util

func IsGeminiArgon(model string) bool {
	switch model {
	case "gemini-4-argon", "gemini-4-argon-low", "gemini-4-argon-medium", "gemini-4-argon-high":
		return true
	}
	return false
}
