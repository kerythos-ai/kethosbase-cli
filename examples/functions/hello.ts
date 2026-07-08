// A minimal Kethosbase Edge Function.
//
// Deploy with:  kethosbase functions deploy examples/functions/hello.ts
// Or just build the .wasm without uploading:
//   kethosbase functions deploy examples/functions/hello.ts --dry-run -o hello.wasm
//
// The @kethosbase/functions SDK speaks the request/response envelope and wraps
// the platform host functions (db, fetch, secret, log). Inside the runtime the
// query is RLS-bound and never elevated to service_role.

import { serve, db, log } from "@kethosbase/functions";

serve(async (req) => {
  log("request", req.method, req.path);

  // RLS-bound query — returns only rows the caller may see.
  const rows = db.query("select now() as now");

  return {
    status: 200,
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ hello: "world", now: rows[0]?.now }),
  };
});
