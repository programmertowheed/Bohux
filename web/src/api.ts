import type { Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { getToken } from "./auth";

// Every call goes to the public API (rule 8) on this origin, with the tab's token.
const withToken: Interceptor = (next) => async (req) => {
  const token = getToken();
  if (token) req.header.set("Authorization", `Bearer ${token}`);
  return next(req);
};

export const transport = createConnectTransport({
  baseUrl: window.location.origin,
  interceptors: [withToken],
});
