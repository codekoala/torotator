IMG := codekoala/torotator
VERSION ?= $(shell /bin/sh -c "git describe --long | sed 's/\([^-]*-g\)/r\1/;s/-/./g'" )

build:
	go build -ldflags '-s -X main.VERSION=$(VERSION)' -o torotator ./cmd

docker:
	docker build --pull -t $(IMG):latest .
	docker tag $(IMG):latest $(IMG):$(VERSION)

run:
	docker run -it --rm -p 8080:8080 -u 1000 $(IMG)

upload:
	docker push $(IMG)

clean:
	rm -f torotator
