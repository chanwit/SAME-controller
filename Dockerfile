# Build debugger
FROM golang:1.16 as debugger
RUN go get -u github.com/go-delve/delve/cmd/dlv

# Build the manager binary
FROM golang:1.16 as builder

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY main.go main.go
COPY api/ api/
COPY controllers/ controllers/

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GO111MODULE=on go build -gcflags "all=-N -l" -a -o manager main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM python:3.8-buster
RUN  python -m pip install --upgrade pip
RUN  python -m pip install -U setuptools wheel pip
RUN  pip install kfp

RUN  curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" \
     && mv ./kubectl /usr/bin/kubectl \
     && chmod +x /usr/bin/kubectl

# COPY ./same /usr/bin/same
RUN curl -L0 https://get.sameproject.org/ | bash -

WORKDIR /
COPY --from=builder /workspace/manager .
COPY --from=debugger /go/bin/dlv .

ENTRYPOINT ["/manager"]
