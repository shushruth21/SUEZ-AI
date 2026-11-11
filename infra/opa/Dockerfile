FROM openpolicyagent/opa:1.22.2

COPY policies /policy/suez
COPY data.json /policy/data.json

# Log level stays at error: debug and decision logs would write every
# decision input, which includes prompt content, to the container log.
# Test files are not loaded into the running server.
CMD ["run", "--server", "--addr=0.0.0.0:8181", "--log-level=error", "--ignore=*_test.rego", "/policy"]
