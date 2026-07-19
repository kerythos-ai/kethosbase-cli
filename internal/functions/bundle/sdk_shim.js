// Fallback in-CLI implementation of the `@kethosbase/functions` SDK.
//
// The real SDK is published from the kethosbase-js repo. Until a project has it
// installed in node_modules, the bundler resolves `@kethosbase/functions` to
// this shim so `functions deploy` works end-to-end. It implements EXACTLY the
// shared bridge contract: it only ever touches the plugin-provided globals
// __kb_read_request / __kb_write_response / __kb_log / __kb_db_query /
// __kb_fetch / __kb_get_secret / __kb_sign. It never touches raw stdio.
//
// When the real @kethosbase/functions package is present in node_modules the
// bundler uses that instead; keep this shim's public surface a subset of it.
//
// TEMPORARILY NOT A SUBSET: `crypto` (crypto.subtle.sign) and `keyFromSecret`
// below land here first. Until @kethosbase/functions ships the same two exports
// with identical semantics, a project that has the npm package installed gets a
// NARROWER runtime than one that falls back to this shim — signing code written
// against the shim breaks on `npm i @kethosbase/functions`. Mirror them in
// kethosbase-js/packages/functions before advertising signing in the docs.

/* global __kb_read_request, __kb_write_response, __kb_log, __kb_db_query, __kb_fetch, __kb_get_secret, __kb_sign */

function b64encode(bytes) {
  // bytes: Uint8Array -> base64 string.
  const table = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
  let out = "";
  for (let i = 0; i < bytes.length; i += 3) {
    const b0 = bytes[i];
    const b1 = i + 1 < bytes.length ? bytes[i + 1] : 0;
    const b2 = i + 2 < bytes.length ? bytes[i + 2] : 0;
    out += table[b0 >> 2];
    out += table[((b0 & 3) << 4) | (b1 >> 4)];
    out += i + 1 < bytes.length ? table[((b1 & 15) << 2) | (b2 >> 6)] : "=";
    out += i + 2 < bytes.length ? table[b2 & 63] : "=";
  }
  return out;
}

function b64decode(str) {
  const table = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
  const lookup = {};
  for (let i = 0; i < table.length; i++) lookup[table[i]] = i;
  const clean = str.replace(/=+$/, "");
  const out = [];
  let bits = 0;
  let acc = 0;
  for (const ch of clean) {
    if (!(ch in lookup)) continue;
    acc = (acc << 6) | lookup[ch];
    bits += 6;
    if (bits >= 8) {
      bits -= 8;
      out.push((acc >> bits) & 0xff);
    }
  }
  return new Uint8Array(out);
}

// Pure-JS UTF-8 encode/decode. QuickJS (the Javy runtime) does NOT provide
// TextEncoder/TextDecoder/atob/btoa/Buffer/crypto — referencing any of them (even
// `new TextEncoder()` at module scope) throws at boot. So we implement UTF-8 and
// base64 by hand and touch no browser/Node global anywhere in this file.

// utf8Encode(str) -> Uint8Array
function utf8Encode(str) {
  const out = [];
  for (let i = 0; i < str.length; i++) {
    let code = str.charCodeAt(i);
    // Combine surrogate pairs into a single code point.
    if (code >= 0xd800 && code <= 0xdbff && i + 1 < str.length) {
      const next = str.charCodeAt(i + 1);
      if (next >= 0xdc00 && next <= 0xdfff) {
        code = 0x10000 + ((code - 0xd800) << 10) + (next - 0xdc00);
        i++;
      }
    }
    if (code < 0x80) {
      out.push(code);
    } else if (code < 0x800) {
      out.push(0xc0 | (code >> 6), 0x80 | (code & 0x3f));
    } else if (code < 0x10000) {
      out.push(0xe0 | (code >> 12), 0x80 | ((code >> 6) & 0x3f), 0x80 | (code & 0x3f));
    } else {
      out.push(
        0xf0 | (code >> 18),
        0x80 | ((code >> 12) & 0x3f),
        0x80 | ((code >> 6) & 0x3f),
        0x80 | (code & 0x3f)
      );
    }
  }
  return new Uint8Array(out);
}

// utf8Decode(Uint8Array) -> str
function utf8Decode(bytes) {
  let out = "";
  let i = 0;
  const n = bytes.length;
  while (i < n) {
    const b0 = bytes[i++];
    let code;
    if (b0 < 0x80) {
      code = b0;
    } else if ((b0 & 0xe0) === 0xc0) {
      code = ((b0 & 0x1f) << 6) | (bytes[i++] & 0x3f);
    } else if ((b0 & 0xf0) === 0xe0) {
      code = ((b0 & 0x0f) << 12) | ((bytes[i++] & 0x3f) << 6) | (bytes[i++] & 0x3f);
    } else {
      code =
        ((b0 & 0x07) << 18) |
        ((bytes[i++] & 0x3f) << 12) |
        ((bytes[i++] & 0x3f) << 6) |
        (bytes[i++] & 0x3f);
    }
    if (code > 0xffff) {
      // Emit as a surrogate pair.
      code -= 0x10000;
      out += String.fromCharCode(0xd800 + (code >> 10), 0xdc00 + (code & 0x3ff));
    } else {
      out += String.fromCharCode(code);
    }
  }
  return out;
}

function toBytes(body) {
  if (body == null) return null;
  if (body instanceof Uint8Array) return body;
  if (typeof body === "string") return utf8Encode(body);
  // Objects are JSON-encoded for convenience.
  return utf8Encode(JSON.stringify(body));
}

// log(...args): append a line to the invocation log.
export function log(...args) {
  const msg = args
    .map((a) => (typeof a === "string" ? a : JSON.stringify(a)))
    .join(" ");
  __kb_log(msg);
}

// db.query(sql, args?): run an RLS-bound SQL query; returns { rows } or throws.
export const db = {
  query(sql, args = []) {
    const staged = __kb_db_query(JSON.stringify({ sql, args }));
    const res = JSON.parse(staged);
    if (res.error) throw new Error("db.query: " + res.error);
    return res.rows;
  },
};

// fetch(url, init?): egress-policed fetch. Returns { status, headers, body }
// where body is a Uint8Array. Throws on host/egress error.
export async function fetch(url, init = {}) {
  const bodyBytes = toBytes(init.body);
  const req = {
    method: init.method || "GET",
    url,
    headers: init.headers || {},
    body: bodyBytes ? b64encode(bodyBytes) : undefined,
  };
  const staged = __kb_fetch(JSON.stringify(req));
  const res = JSON.parse(staged);
  if (res.error) throw new Error("fetch: " + res.error);
  return {
    status: res.status,
    headers: res.headers || {},
    body: res.body ? b64decode(res.body) : new Uint8Array(0),
    text() {
      return utf8Decode(this.body);
    },
    json() {
      return JSON.parse(utf8Decode(this.body));
    },
  };
}

// secret(name): fetch a sealed secret value; throws if unavailable.
export function secret(name) {
  const staged = __kb_get_secret(String(name));
  const res = JSON.parse(staged);
  if (res.error) throw new Error("secret: " + res.error);
  return res.value;
}

// ---- signing (ADR-0126) ---------------------------------------------------
//
// The private key NEVER enters this runtime. keyFromSecret(name) returns an
// opaque, CryptoKey-LIKE handle carrying only the NAME of the Function secret
// that holds the PEM; the host unseals that secret, parses the PEM and signs in
// HOST memory, so key material never reaches WASM linear memory. There is
// deliberately no importKey()/key-by-value path — adding one would defeat the
// isolation the whole host ABI is built around.

const KB_SECRET_NAME = "__kbSecretName";

// keyFromSecret(name): an opaque handle naming the Function secret that holds a
// PEM private key. Pass it as the `key` argument to crypto.subtle.sign().
export function keyFromSecret(name) {
  if (typeof name !== "string" || name === "") {
    throw new Error(
      "keyFromSecret: expected the name of a Function secret holding a PEM private key"
    );
  }
  return {
    type: "private",
    extractable: false,
    usages: ["sign"],
    [KB_SECRET_NAME]: name,
  };
}

function hashName(h) {
  if (typeof h === "string") return h;
  if (h && typeof h.name === "string") return h.name;
  return undefined;
}

// Map a WebCrypto algorithm to the host's `alg`. Unsupported inputs throw here,
// before anything is sent to the host — never silently default.
function toHostAlg(algorithm) {
  const name = typeof algorithm === "string" ? algorithm : algorithm && algorithm.name;
  const hash = typeof algorithm === "string" ? undefined : hashName(algorithm && algorithm.hash);
  if (name === "RSASSA-PKCS1-v1_5") {
    // For RSASSA-PKCS1-v1_5 WebCrypto carries the hash on the key, so `hash` is
    // optional here; if it is given it must be SHA-256.
    if (hash !== undefined && hash !== "SHA-256") {
      throw new Error(
        'crypto.subtle.sign: RSASSA-PKCS1-v1_5 supports only SHA-256, got "' + hash + '"'
      );
    }
    return "RS256";
  }
  if (name === "ECDSA") {
    if (hash !== "SHA-256") {
      throw new Error(
        'crypto.subtle.sign: ECDSA requires hash "SHA-256" on a P-256 key (ES256), got "' +
          String(hash) +
          '"'
      );
    }
    return "ES256";
  }
  throw new Error(
    "crypto.subtle.sign: unsupported algorithm " +
      JSON.stringify(name === undefined ? algorithm : name) +
      ' (supported: {name:"RSASSA-PKCS1-v1_5"} for RS256, {name:"ECDSA",hash:"SHA-256"} for ES256)'
  );
}

function secretNameFromKey(key) {
  // Reject raw key material outright: transporting a private key through JS
  // would put it in WASM memory, which is exactly what this design prevents.
  if (typeof key === "string" || key instanceof ArrayBuffer || ArrayBuffer.isView(key)) {
    throw new Error(
      "crypto.subtle.sign: raw key material is not accepted — a private key must never " +
        'enter the Function runtime. Store the PEM as a Function secret and pass keyFromSecret("SECRET_NAME").'
    );
  }
  const name = key && key[KB_SECRET_NAME];
  if (typeof name !== "string" || name === "") {
    throw new Error(
      'crypto.subtle.sign: key must be a handle from keyFromSecret("SECRET_NAME")'
    );
  }
  return name;
}

function toSignBytes(data) {
  if (data instanceof Uint8Array) return data;
  if (typeof data === "string") return utf8Encode(data);
  if (data instanceof ArrayBuffer) return new Uint8Array(data);
  if (ArrayBuffer.isView(data)) return new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
  throw new Error("crypto.subtle.sign: data must be a string, ArrayBuffer, or typed array");
}

// crypto.subtle.sign(algorithm, key, data) -> Promise<ArrayBuffer>
//
// A deliberately partial WebCrypto surface: sign() only, over the host's RS256
// and ES256. ES256 signatures come back as fixed-width r||s (the JWS encoding),
// not ASN.1 DER, so they are passed through unchanged.
export const crypto = {
  subtle: {
    async sign(algorithm, key, data) {
      const alg = toHostAlg(algorithm);
      const keyName = secretNameFromKey(key);
      const bytes = toSignBytes(data);
      const staged = __kb_sign(JSON.stringify({ alg, key: keyName, data: b64encode(bytes) }));
      let res;
      try {
        res = JSON.parse(staged);
      } catch (e) {
        throw new Error("crypto.subtle.sign: host returned malformed JSON");
      }
      if (res.error) throw new Error("crypto.subtle.sign: " + res.error);
      if (typeof res.signature !== "string") {
        throw new Error("crypto.subtle.sign: host returned no signature");
      }
      return b64decode(res.signature).buffer;
    },
  },
};

// serve(handler): read the request envelope, invoke handler(req), and write the
// response envelope. handler may be sync or async and returns a Response-like
// object { status, headers?, body? } (body: string | Uint8Array | object).
export function serve(handler) {
  const run = async () => {
    let raw;
    try {
      raw = __kb_read_request();
    } catch (e) {
      writeResponse({ status: 500, body: "failed to read request" });
      return;
    }
    let req;
    try {
      const env = JSON.parse(raw);
      req = {
        method: env.method,
        path: env.path,
        query: env.query || "",
        headers: env.headers || {},
        body: env.body ? b64decode(env.body) : new Uint8Array(0),
        text() {
          return utf8Decode(this.body);
        },
        json() {
          return JSON.parse(utf8Decode(this.body));
        },
      };
    } catch (e) {
      writeResponse({ status: 400, body: "invalid request envelope" });
      return;
    }
    try {
      const res = await handler(req);
      writeResponse(res || { status: 204 });
    } catch (e) {
      log("unhandled error: " + (e && e.stack ? e.stack : String(e)));
      writeResponse({ status: 500, body: "internal error" });
    }
  };
  run();
}

function writeResponse(res) {
  const status = typeof res.status === "number" ? res.status : 200;
  const bodyBytes = toBytes(res.body);
  const env = {
    status,
    headers: res.headers || undefined,
    body: bodyBytes ? b64encode(bodyBytes) : undefined,
  };
  __kb_write_response(JSON.stringify(env));
}

export default { serve, db, fetch, secret, log, crypto, keyFromSecret };
