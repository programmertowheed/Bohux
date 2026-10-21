import { createRootRoute, createRoute, createRouter, Link, Outlet } from "@tanstack/react-router";
import { useQuery } from "@connectrpc/connect-query";
import { IdentityService, PrincipalKind } from "./gen/bohux/v1/identity_pb";
import { clearToken, getToken } from "./auth";
import { SignIn } from "./pages/SignIn";
import { Projects } from "./pages/Projects";
import { Environments } from "./pages/Environments";
import { Environment } from "./pages/Environment";
import { Nodes } from "./pages/Nodes";
import { Deployment } from "./pages/Deployment";
import { Logs } from "./pages/Logs";
import { Activity } from "./pages/Activity";
import { enumName } from "./format";

function Shell() {
  if (!getToken()) return <SignIn />;
  return <SignedIn />;
}

function SignedIn() {
  const me = useQuery(IdentityService.method.whoAmI, {});
  if (me.error) return <SignIn error="That token was not accepted." />;
  const p = me.data?.principal;
  return (
    <div className="min-h-screen">
      <header className="flex items-center gap-6 border-b border-border px-6 py-3">
        <Link to="/" className="font-semibold">Bohux</Link>
        <Link to="/" className="text-muted [&.active]:text-foreground">Projects</Link>
        <Link to="/nodes" className="text-muted [&.active]:text-foreground">Nodes</Link>
        <span className="ml-auto text-sm text-muted">
          {p ? `${p.displayName || p.id} (${enumName("PRINCIPAL_KIND_", PrincipalKind[p.kind])})` : ""}
        </span>
        <button
          className="text-sm text-muted hover:text-foreground"
          onClick={() => {
            clearToken();
            window.location.assign("/ui/");
          }}
        >
          Sign out
        </button>
      </header>
      <main className="mx-auto max-w-6xl px-6 py-6">
        <Outlet />
      </main>
    </div>
  );
}

const root = createRootRoute({ component: Shell });
const projects = createRoute({ getParentRoute: () => root, path: "/", component: Projects });
const nodes = createRoute({ getParentRoute: () => root, path: "/nodes", component: Nodes });
const project = createRoute({ getParentRoute: () => root, path: "/p/$project", component: Environments });
const environment = createRoute({ getParentRoute: () => root, path: "/p/$project/e/$env", component: Environment });
const deployment = createRoute({
  getParentRoute: () => root,
  path: "/p/$project/e/$env/d/$id",
  component: Deployment,
});

const logs = createRoute({ getParentRoute: () => root, path: "/p/$project/e/$env/a/$app/logs", component: Logs });

const activity = createRoute({ getParentRoute: () => root, path: "/p/$project/activity", component: Activity });

export const router = createRouter({
  routeTree: root.addChildren([projects, nodes, project, environment, deployment, logs, activity]),
  basepath: "/ui",
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
