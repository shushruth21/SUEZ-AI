import { describe, expect, it } from "vitest";
import { gatewayUrlProblem } from "./gatewayUrl";

describe("gatewayUrlProblem", () => {
  it("accepts https anywhere and http on loopback", () => {
    for (const ok of [
      "https://suez.example.com:5443/gateway",
      "http://localhost:8080",
      "http://127.0.0.1:8080/",
      "http://[::1]:8080",
      "http://suez.localhost:8080"
    ]) {
      expect(gatewayUrlProblem(ok), ok).toBeNull();
    }
  });

  it("rejects plain http to other hosts and unsafe URLs", () => {
    for (const bad of [
      "",
      "localhost:8080",
      "ftp://host",
      "http://192.168.1.20:8080",
      "http://gateway.example.com",
      "http://localhost.evil.com",
      "https://user:pw@host",
      "https://host/?q=1",
      "https://host/#x"
    ]) {
      expect(gatewayUrlProblem(bad), bad).not.toBeNull();
    }
  });
});
