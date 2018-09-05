.PHONY: image push dev_image dev_push fix_gcs_permissions

DEV_PREFIX ?= gcr.io/$(shell gcloud config get-value project)
NAME = gce-controller-manager
TAG = 0.0.1

dev_image:
	docker build -t "$(DEV_PREFIX)/$(NAME):$(TAG)-dev" -f ./Dockerfile ../../../..

dev_push: dev_image
	docker push "$(DEV_PREFIX)/$(NAME):$(TAG)-dev"
