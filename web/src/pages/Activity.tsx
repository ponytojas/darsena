import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { es as t } from "@/i18n/es";
import {
  PageTitle,
  NewAppButton,
  Loading,
  Failure,
  Blank,
  OperationRows,
} from "@/components/shared";
export default function ActivityPage() {
  const operations = useQuery({
    queryKey: ["operations"],
    queryFn: api.operations,
    refetchInterval: (query) =>
      query.state.data?.some(
        (op) => op.status === "queued" || op.status === "running",
      )
        ? 2000
        : false,
  });
  const apps = useQuery({ queryKey: ["applications"], queryFn: api.apps });
  return (
    <div className="page">
      <PageTitle
        eyebrow={t.overview}
        title={t.activity}
        description={t.activityDescription}
        action={<NewAppButton />}
      />
      {operations.isPending ? (
        <Loading />
      ) : operations.isError ? (
        <Failure
          error={operations.error}
          retry={() => void operations.refetch()}
        />
      ) : operations.data.length ? (
        <OperationRows operations={operations.data} apps={apps.data} />
      ) : (
        <Blank
          title={t.noActivity}
          description={t.noActivityDescription}
          action={<NewAppButton />}
        />
      )}
    </div>
  );
}
