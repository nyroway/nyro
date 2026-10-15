import { StrictMode, Suspense, lazy } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter, Routes, Route } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { AppLayout } from "@/components/layout/app-layout";
import { AppErrorBoundary } from "@/components/error-boundary";
import { HoverTipHost } from "@/components/ui/hover-tip";
import { ToastHost } from "@/components/ui/toast";
import { LocaleProvider } from "@/lib/i18n";

import "./styles/nyro-ui.css";
import "./styles/radix-bridge.css";
import "./styles/nyro-app.css";
import "./styles/nyro-console.css";

const DashboardPage = lazy(() => import("@/pages/dashboard"));
const ProvidersPage = lazy(() => import("@/pages/providers"));
const ModelsPage = lazy(() => import("@/pages/models"));
const ApiKeysPage = lazy(() => import("@/pages/api-keys"));
const LogsPage = lazy(() => import("@/pages/logs"));
const StatsPage = lazy(() => import("@/pages/stats"));
const SettingsPage = lazy(() => import("@/pages/settings"));
const ConnectPage = lazy(() => import("@/pages/connect"));
const LoginPage = lazy(() => import("@/pages/login"));

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchOnWindowFocus: false,
      retry: 1,
      staleTime: 10_000,
    },
  },
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <AppErrorBoundary>
      <QueryClientProvider client={queryClient}>
        <LocaleProvider>
          <BrowserRouter>
            <Suspense fallback={<div className="app-loading">Loading...</div>}>
              <Routes>
                <Route path="login" element={<LoginPage />} />
                <Route element={<AppLayout />}>
                  <Route index element={<DashboardPage />} />
                  <Route path="providers" element={<ProvidersPage />} />
                  <Route path="models" element={<ModelsPage />} />
                  <Route path="api-keys" element={<ApiKeysPage />} />
                  <Route path="logs" element={<LogsPage />} />
                  <Route path="stats" element={<StatsPage />} />
                  <Route path="connect" element={<ConnectPage />} />
                  <Route path="settings" element={<SettingsPage />} />
                </Route>
              </Routes>
            </Suspense>
          </BrowserRouter>
          <ToastHost />
          <HoverTipHost />
        </LocaleProvider>
      </QueryClientProvider>
    </AppErrorBoundary>
  </StrictMode>
);
