OPENCLOUD_DIR := ../opencloud
APIDOC := $(OPENCLOUD_DIR)/services/groupware/apidoc.yml

.PHONY: all
all: api

.PHONY: examples
examples:
		$(MAKE) -C $(OPENCLOUD_DIR)/services/groupware/ examples

.PHONY: api
api:	examples
	go run . -C $(OPENCLOUD_DIR)/ -t $(APIDOC) > $(OPENCLOUD_DIR)/services/groupware/openapi.yml
	$(MAKE) -C $(OPENCLOUD_DIR)/services/groupware/ api.html

