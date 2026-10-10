FROM openpolicyagent/opa:1.4.2

COPY policies /policy/suez
COPY data.json /policy/data.json

CMD ["run", "--server", "--addr=0.0.0.0:8181", "--log-level=debug", "/policy"]
