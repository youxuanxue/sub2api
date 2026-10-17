package service

const (
	saturationWindowSeconds            = 600
	saturationThreshold          int64 = 5
	saturationPriorityPenalty          = 1000
	openAISaturationScorePenalty       = 50.0
)

const (
	anthropicSaturationThreshold = saturationThreshold
	openAISaturationThreshold    = saturationThreshold
)
