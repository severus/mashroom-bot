PROJECT_ID ?= $(shell gcloud config get-value project)
SERVICE = mashroom-bot
BOT_TOKEN = TELEGRAM_BOT_TOKEN_HERE

.PHONY: build
build:
	gcloud builds submit \
	  --tag gcr.io/$(PROJECT_ID)/$(SERVICE)

.PHONY: deploy
deploy:
	gcloud run deploy $(SERVICE) \
	  --image gcr.io/$(PROJECT_ID)/$(SERVICE) \
	  --platform managed \
	  --region us-central1 \
	  --set-env-vars "BOT_TOKEN=$(BOT_TOKEN)"

.PHONY: webhook
webhook: WEBHOOK_URL = $(shell gcloud run services describe $(SERVICE) --platform managed --format json | jq -r .status.url)
webhook:
	curl https://api.telegram.org/bot$(BOT_TOKEN)/setWebhook?url=$(WEBHOOK_URL)
