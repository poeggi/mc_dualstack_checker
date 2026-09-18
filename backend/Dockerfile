FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /mc_dualstack_check .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /mc_dualstack_check /mc_dualstack_check
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/mc_dualstack_check"]
