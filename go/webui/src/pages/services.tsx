import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";

import { PageHeader } from "@/components/v2/page-header";
import { PageLayout } from "@/components/v2/page-layout";
import { RuntimeServicesView } from "@/features/services/runtime-services-view";
import { backend } from "@/lib/backend";
import { useLocale } from "@/lib/i18n";
import type { RuntimeService } from "@/lib/types";

export default function ServicesPage() {
  const { t } = useLocale();
  const navigate = useNavigate();
  const { data: services = [], isLoading, isError, isFetching, refetch } = useQuery<RuntimeService[]>({
    queryKey: ["runtime-services"],
    queryFn: () => backend("list_runtime_services"),
    refetchInterval: 30_000,
  });

  return (
    <PageLayout header={<PageHeader title={t("page.services.title")} description={t("page.services.subtitle")} />}>
      <RuntimeServicesView
        services={services}
        isLoading={isLoading}
        isError={isError}
        isFetching={isFetching}
        t={t}
        onRefresh={() => void refetch()}
        onShowNodes={() => navigate("/nodes")}
      />
    </PageLayout>
  );
}
