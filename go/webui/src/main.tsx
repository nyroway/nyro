import { StrictMode, Suspense, lazy } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter, Routes, Route } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { AppLayout } from "@/components/layout/app-layout";
import { AppErrorBoundary } from "@/components/error-boundary";
import { LocaleProvider } from "@/lib/i18n";

import "./index.css";
import "./styles/v2.css";

const DashboardPage = lazy(() => import("@/pages/dashboard"));
const ProvidersPage = lazy(() => import("@/pages/providers"));
const ModelsPage = lazy(() => import("@/pages/models-v2"));
const ApiKeysPage = lazy(() => import("@/pages/api-keys"));
const ConnectPage = lazy(() => import("@/pages/connect"));
const NodesPage = lazy(() => import("@/pages/nodes"));
const ServicesPage = lazy(() => import("@/pages/services"));
const LogsPage = lazy(() => import("@/pages/logs"));
const StatsPage = lazy(() => import("@/pages/stats-v2"));
const SettingsPage = lazy(() => import("@/pages/settings"));

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
            <Suspense fallback={<div className="p-6 text-sm text-slate-500">Loading...</div>}>
              <Routes>
                <Route element={<AppLayout />}>
                  <Route index element={<DashboardPage />} />
                  <Route path="providers" element={<ProvidersPage />} />
                  <Route path="models" element={<ModelsPage />} />
                  <Route path="api-keys" element={<ApiKeysPage />} />
                  <Route path="connect" element={<ConnectPage />} />
                  <Route path="nodes" element={<NodesPage />} />
                  <Route path="services" element={<ServicesPage />} />
                  <Route path="logs" element={<LogsPage />} />
                  <Route path="stats" element={<StatsPage />} />
                  <Route path="settings" element={<SettingsPage />} />
                  <Route path="*" element={<DashboardPage />} />
                </Route>
              </Routes>
            </Suspense>
          </BrowserRouter>
        </LocaleProvider>
      </QueryClientProvider>
    </AppErrorBoundary>
  </StrictMode>
);
