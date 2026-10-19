module github.com/hasan-muzak/promptdam/detector

go 1.27.2

replace (
	github.com/hasan-muzak/keel-core => ../../../../core
	github.com/hasan-muzak/promptdam => ../
)

require (
	github.com/daulet/tokenizers v1.27.0
	github.com/hasan-muzak/promptdam v0.0.0
	github.com/yalue/onnxruntime_go v1.36.0
)
