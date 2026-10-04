.PHONY: build-DailyWorkerFunction build-StatusHandlerFunction build-SheetSyncFunction

build-DailyWorkerFunction:
	test -n "$(ARTIFACTS_DIR)"
	mkdir -p "$(ARTIFACTS_DIR)"
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags lambda.norpc -trimpath -ldflags="-s -w" -o "$(ARTIFACTS_DIR)/bootstrap" ./cmd/daily-worker

build-StatusHandlerFunction:
	test -n "$(ARTIFACTS_DIR)"
	mkdir -p "$(ARTIFACTS_DIR)"
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags lambda.norpc -trimpath -ldflags="-s -w" -o "$(ARTIFACTS_DIR)/bootstrap" ./cmd/status-handler

build-SheetSyncFunction:
	test -n "$(ARTIFACTS_DIR)"
	mkdir -p "$(ARTIFACTS_DIR)"
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags lambda.norpc -trimpath -ldflags="-s -w" -o "$(ARTIFACTS_DIR)/bootstrap" ./cmd/sheet-sync
