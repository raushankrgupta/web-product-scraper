package utils

import "strings"

func AddToLogMessage(logMessagesBuilder *strings.Builder, strToAdd string) {

	if logMessagesBuilder.Len() == logMessagesBuilder.Cap() {

		logMessagesBuilder.Grow(len(strToAdd))
	}

	logMessagesBuilder.WriteString(strToAdd)
	logMessagesBuilder.WriteString(";")
	logMessagesBuilder.WriteString("\n")
}
