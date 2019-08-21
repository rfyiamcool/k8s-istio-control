DEFAULT_GOAL := build-all

build-all: clean build

clean:
	@rm -f control

build:
	@go build -o control control.go

start:
	@./control start

stop:
	@./control stop

status:
	@./control status

gen:
	@./control gen

pods:
	@./control pods
