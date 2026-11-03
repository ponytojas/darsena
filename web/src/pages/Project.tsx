import { Link, useParams } from "react-router";
import { Button } from "@/components/ui/button";
import { Blank, Failure, Loading } from "@/components/shared";
import { useI18n } from "@/i18n/context";
import { useApplications } from "@/lib/applications";
import { useCapabilities } from "@/lib/capabilities";
import Applications from "@/pages/Applications";

/** `/projects/:id`: the applications list scoped to one project. */
export default function ProjectPage() {
  const { t } = useI18n();
  const { id = "" } = useParams();
  const capabilities = useCapabilities();
  const { projects, projectsPending, projectsFailed, projectsError, refetchProjects } = useApplications();
  const back = <Button render={<Link to="/" />}>{t.backHome}</Button>;
  if (!capabilities.data) return <Loading />;
  if (!capabilities.workerReady)
    return (
      <div className="page">
        <Blank title={t.projects.unavailableTitle} description={t.projects.unavailableDescription} action={back} />
      </div>
    );
  if (projectsPending) return <Loading />;
  if (projectsFailed)
    return (
      <div className="page">
        <Failure error={projectsError} retry={refetchProjects} />
      </div>
    );
  const project = projects.find((candidate) => candidate.id === id);
  if (!project)
    return (
      <div className="page">
        <Blank title={t.projects.notFoundTitle} description={t.projects.notFoundDescription} action={back} />
      </div>
    );
  return <Applications project={project} />;
}
