package tags

// cleanTextForTest exposes the tag tidying to the black-box tests next door.
func CleanTextForTest(value string) string { return cleanText(value) }
