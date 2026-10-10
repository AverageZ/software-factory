PYTHON ?= python3
export PYTHONDONTWRITEBYTECODE := 1

.PHONY: bootstrap build dev dev-ui test lint fmt check-fmt pre-commit

bootstrap build test lint fmt check-fmt pre-commit:
	$(PYTHON) scripts/tooling.py $@

dev: build
	$(PYTHON) scripts/dev.py

dev-ui:
	npm --prefix web run dev:ui
