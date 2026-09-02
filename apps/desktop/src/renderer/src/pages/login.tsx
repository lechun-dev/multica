import { DingTalkFirstLoginFrame, LoginPage } from "@multica/views/auth";
import { DragStrip } from "@multica/views/platform";
import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { buildDingTalkLoginURL } from "@multica/core/auth";
import { resolveDesktopIdentity } from "../../../shared/desktop-identity";

function requireRuntimeApiUrl(): string {
  const runtimeConfig = window.desktopAPI.runtimeConfig;
  if (!runtimeConfig.ok) {
    throw new Error(
      "Invariant violated: DesktopLoginPage rendered before App accepted runtime config",
    );
  }
  return runtimeConfig.config.apiUrl;
}

export function DesktopLoginPage() {
  const apiUrl = requireRuntimeApiUrl();
  const handleDingTalkLogin = () => {
    const identity = resolveDesktopIdentity({
      isDev: import.meta.env.DEV,
      variant: import.meta.env.VITE_MULTICA_DESKTOP_VARIANT,
    });
    window.desktopAPI.openExternal(
      buildDingTalkLoginURL(apiUrl, identity.oauthClient),
    );
  };

  return (
    <div className="flex h-screen flex-col">
      <DragStrip />
      <DingTalkFirstLoginFrame>
        <LoginPage
          logo={<MulticaIcon bordered size="lg" />}
          hideEmailLogin
          onSuccess={() => {
            // Auth store update triggers AppContent re-render → shows DesktopShell.
            // Initial workspace navigation happens in routes.tsx via IndexRedirect.
          }}
          onDingTalkLogin={handleDingTalkLogin}
        />
      </DingTalkFirstLoginFrame>
    </div>
  );
}
