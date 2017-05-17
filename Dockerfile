FROM golang:1.8 as builder
WORKDIR /go/src/github.com/nailgun/k8s-hostpath-provisioner/
RUN go get -d -v golang.org/x/net/html  
COPY . .
RUN make

FROM scratch
COPY --from=builder /go/src/github.com/nailgun/k8s-hostpath-provisioner/hostpath-provisioner /
ENTRYPOINT ["/hostpath-provisioner"]
