#!/usr/bin/env python3
"""Exercise real Keycloak Code+PKCE, profile and shared API audience in isolation."""
import base64
from html.parser import HTMLParser
import hashlib
import http.cookiejar
import importlib.util
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import ssl
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

spec = importlib.util.spec_from_file_location("events", Path(__file__).with_name("verify-events.py"))
e = importlib.util.module_from_spec(spec)
spec.loader.exec_module(e)
e.COMPOSE[e.COMPOSE.index("food-events-e2e")] = "food-auth-e2e"
e.COMPOSE += ["-f", str(e.ROOT / "deploy/compose.oidc.yaml")]
KEYCLOAK_PORT = os.getenv("TEST_KEYCLOAK_PORT", "28443")
IDENTITY_URL = "https://identity.food.test:" + KEYCLOAK_PORT
ISSUER = IDENTITY_URL + "/realms/food"
ORIGIN = "http://localhost:9000"
REDIRECT = ORIGIN + "/auth/callback"


class LoginForm(HTMLParser):
    def __init__(self):
        super().__init__()
        self.action = None

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == "form" and attrs.get("id") == "kc-form-login":
            self.action = attrs["action"]


class StopRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def send(opener, url, method="GET", data=None, headers=None, expected=200):
    req = urllib.request.Request(url, method=method, data=data, headers=headers or {})
    try:
        response = opener.open(req, timeout=15)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        # Never include token bodies, passwords or Firebase identifiers in failures.
        assert response.code == expected, f"{method} {urllib.parse.urlsplit(url).path}: {response.code}, expected {expected}"
        return response.read(), response.headers


def form(values):
    return urllib.parse.urlencode(values).encode()


def main():
    original_resolver = socket.getaddrinfo

    def resolve(host, *args, **kwargs):
        return original_resolver("127.0.0.1" if host == "identity.food.test" else host, *args, **kwargs)

    # Preserve the HTTPS hostname/SNI while reaching the loopback-only IdP port.
    socket.getaddrinfo = resolve
    with tempfile.TemporaryDirectory(prefix="food-auth-") as directory:
        root = Path(directory)
        tls = root / "tls"
        tls.mkdir()
        cert, key = tls / "server.pem", tls / "server-key.pem"
        subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
                        "-keyout", str(key), "-out", str(cert), "-days", "1",
                        "-subj", "/CN=identity.food.test", "-addext", "subjectAltName=DNS:identity.food.test"],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        # The containing temporary directory is mode 0700 on the host. Only its
        # TLS child is mounted into Keycloak, whose UID needs to read this key.
        key.chmod(0o644)
        shutil.copyfile(cert, root / "ca.pem")
        registry = root / "identities.json"
        registry.write_text(json.dumps({"issuer": ISSUER, "identities": []}))
        admin_password, staff_password = secrets.token_urlsafe(32), secrets.token_urlsafe(32)
        e.ENV.update(OIDC_DEV_DIR=str(root), KEYCLOAK_ADMIN_USER="local-admin",
                     KEYCLOAK_ADMIN_PASSWORD=admin_password, PUSH_PROVIDER="fake",
                     CORS_ALLOWED_ORIGINS=ORIGIN, KEYCLOAK_PORT=KEYCLOAK_PORT,
                     KEYCLOAK_PUBLIC_URL=IDENTITY_URL)
        tls_context = ssl.create_default_context(cafile=str(cert))

        def opener(redirects=True):
            handlers = [urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=tls_context),
                        urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar())]
            if not redirects:
                handlers.append(StopRedirect())
            return urllib.request.build_opener(*handlers)

        client = opener()
        try:
            e.compose("up", "-d", "keycloak")
            deadline = time.monotonic() + 180
            while True:
                try:
                    raw, _ = send(client, ISSUER + "/.well-known/openid-configuration")
                    assert json.loads(raw)["issuer"] == ISSUER
                    break
                except (urllib.error.URLError, AssertionError):
                    if time.monotonic() >= deadline:
                        raise RuntimeError("local Keycloak did not become ready") from None
                    time.sleep(1)
            raw, _ = send(client, IDENTITY_URL + "/realms/master/protocol/openid-connect/token",
                          "POST", form({"grant_type": "password", "client_id": "admin-cli",
                                        "username": "local-admin", "password": admin_password}))
            admin_headers = {"Authorization": "Bearer " + json.loads(raw)["access_token"], "Content-Type": "application/json"}
            _, headers = send(client, IDENTITY_URL + "/admin/realms/food/users", "POST",
                              json.dumps({"username": "staff", "enabled": True, "email": "staff@example.test",
                                          "firstName": "Local", "lastName": "Staff", "emailVerified": True,
                                          "credentials": [{"type": "password", "value": staff_password, "temporary": False}]}).encode(),
                              admin_headers, 201)
            subject = headers["Location"].rstrip("/").rsplit("/", 1)[1]

            def login():
                browser = opener(False)
                verifier = secrets.token_urlsafe(48)
                challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).rstrip(b"=").decode()
                state = secrets.token_urlsafe(24)
                params = {"client_id": "food-pwa", "redirect_uri": REDIRECT, "response_type": "code",
                          "scope": "openid", "state": state, "nonce": secrets.token_urlsafe(24),
                          "code_challenge": challenge, "code_challenge_method": "S256"}
                raw, _ = send(browser, ISSUER + "/protocol/openid-connect/auth?" + urllib.parse.urlencode(params))
                parsed = LoginForm()
                parsed.feed(raw.decode())
                assert parsed.action, "Keycloak login form missing"
                _, headers = send(browser, parsed.action, "POST", form({"username": "staff", "password": staff_password}), expected=302)
                callback = urllib.parse.urlsplit(headers["Location"])
                assert callback.scheme + "://" + callback.netloc + callback.path == REDIRECT
                result = urllib.parse.parse_qs(callback.query)
                assert result.get("state") == [state] and result.get("iss") == [ISSUER]
                raw, _ = send(browser, ISSUER + "/protocol/openid-connect/token", "POST",
                              form({"grant_type": "authorization_code", "client_id": "food-pwa",
                                    "redirect_uri": REDIRECT, "code": result["code"][0], "code_verifier": verifier}))
                return json.loads(raw)

            tokens = login()
            params = {"client_id": "food-pwa", "redirect_uri": REDIRECT, "response_type": "code", "scope": "openid"}
            _, headers = send(opener(False), ISSUER + "/protocol/openid-connect/auth?" + urllib.parse.urlencode(params), expected=302)
            rejection = urllib.parse.parse_qs(urllib.parse.urlsplit(headers["Location"]).query)
            assert rejection.get("error") == ["invalid_request"], "authorization without PKCE was not rejected"

            # Start with an empty registry: a real authenticated IdP user is 403.
            e.compose("up", "--build", "-d", "--wait", "--wait-timeout", "360")
            tokens = login()
            bearer = {"Authorization": "Bearer " + tokens["access_token"], "Origin": ORIGIN}
            url = "http://localhost:28081/v1/auth/me"
            send(client, url, headers=bearer, expected=403)
            registry.write_text(json.dumps({"issuer": ISSUER, "identities": [{"subject": subject, "user_id": 10,
                                 "roles": ["restaurant_staff"], "restaurants": [1, 2], "permissions": []}]}))
            e.compose("restart", "catalog", "orders", "delivery", "notifications")
            e.compose("up", "-d", "--wait", "--wait-timeout", "180")
            # Obtain a fresh token after potentially long image builds.
            tokens = login()
            bearer["Authorization"] = "Bearer " + tokens["access_token"]
            raw, headers = send(client, url + "?user_id=999", headers=bearer)
            assert json.loads(raw) == {"user_id": 10, "roles": ["restaurant_staff"], "restaurant_ids": [1, 2], "permissions": []}
            assert headers["Cache-Control"] == "no-store" and headers["Access-Control-Allow-Origin"] == ORIGIN
            send(client, url, headers={"Origin": ORIGIN}, expected=401)
            for name in ("id_token", "refresh_token"):
                send(client, url, headers={"Authorization": "Bearer " + tokens[name]}, expected=401)
            send(client, url, headers={**bearer, "X-Dev-User-ID": "10"}, expected=401)
            send(client, url, headers={**bearer, "Origin": "https://evil.example"}, expected=403)
            _, headers = send(client, url, "OPTIONS", headers={"Origin": ORIGIN, "Access-Control-Request-Method": "GET",
                                                               "Access-Control-Request-Headers": "Authorization"}, expected=204)
            assert headers["Access-Control-Allow-Origin"] == ORIGIN
            send(client, "http://localhost:28081/restaurants/1/orders", headers=bearer)
            send(client, "http://localhost:28081/restaurants/999/orders", headers=bearer, expected=403)
            send(client, "http://localhost:28080/restaurants/1/delivery-settings", headers=bearer)
            send(client, "http://localhost:28080/restaurants/999/delivery-settings", headers=bearer, expected=403)
            # Delivery authenticates the shared token, then denies the staff role.
            send(client, "http://localhost:28085/deliveries", headers=bearer, expected=403)
            send(client, "http://localhost:28083/v1/devices", headers=bearer)
            logs = e.compose("logs", "--no-color", "catalog", "orders", "delivery", "notifications", capture=True)
            for private in [*tokens.values(), staff_password, admin_password]:
                if isinstance(private, str) and len(private) > 20:
                    assert private not in logs, "credential appeared in API logs"
            print("PASS: real HTTPS Keycloak Code+PKCE, required S256, registry 403/restart, profile, access-token-only, CORS and shared API audience.", flush=True)
        finally:
            e.compose("down", "-v", "--remove-orphans")
            socket.getaddrinfo = original_resolver


if __name__ == "__main__":
    main()
