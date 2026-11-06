# Dársena

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Dársena is aimed at a single owner who manages applications.

## Product Purpose

Dársena is an open-source PaaS. Its current interface, in Spanish and English (selector in Settings), offers an experimental local space to explore application workflows.

## Operating Context

The main flows start from a single list of applications (grouped, with search and filters) and from the ⌘K palette: open an application and follow its "Next step", simulate a deployment of an example, review activity, create an application by first choosing the type (simulated example or saved definition) and manage definitions: variables, configuration history with restoration of a previous value, settings (project, image, port) and deletion. Definitions are grouped into projects (creation, editing of name and color, deletion with "Undo", and moving individually or in bulk); deleting a project never deletes applications. A Diagnostics page explains the state of the server, the worker and real execution, with the command or document that resolves each gap. The interface queries the server's capabilities to tell an unconfigured worker from an unavailable one, report on the encrypted vault, and show whether experimental real execution is enabled and available. When it cannot confirm the state, it keeps drafts and blocks the actions that depend on the worker or the vault.

## Capabilities and Constraints

Example applications and deployments live in memory and reset with the server; their URLs and results are fictitious. In demo, saved applications keep definitions and variables without deploying services. The authenticated local experimental mode allows an initial creation per application from an image by digest (a public tag is resolved to its digest, and the image can be explicitly downloaded into the runtime), with or without variables, in a single "Deploy" that downloads the image if needed and can create the project on the spot; by default the application is isolated: it publishes no ports and provides no public URL. With "Internal network" access it has a network with outbound Internet access and the applications on its network reach it by name, without publishing anything. With "Open on this computer" it also has its own address, `http://<domain>.localhost`, on this computer only and without authentication: any program or website open in the browser can send it requests. It can also connect to a common network shared between projects. Each domain is unique among applications: if it already exists, creation is rejected and it is never changed silently. An application with a used service cannot change its access or domain. The interface allows stopping the service and deleting the stopped container with confirmation, keeping the definition and configuration; it does not allow recreating it or applying rotations automatically. A desktop flow has been tested with Chromium and a real daemon on OrbStack; the security review and real validation on Safari, on mobile and after restarting with open services are still pending. A definition whose service has ever been created cannot change its image, because service recreation does not exist yet; it can be deleted: if its container still exists, it is stopped and deleted first; in an isolated application the port is metadata and remains editable; in the others it is locked together with the access. Worker jobs are persistent simulations and do not modify infrastructure.
