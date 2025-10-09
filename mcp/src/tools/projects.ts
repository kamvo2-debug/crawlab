import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { CrawlabClient } from "../client.js";
import { z } from "zod";

const PROJECT_TOOLS = {
  list_projects: "crawlab_list_projects",
  get_project: "crawlab_get_project",
  create_project: "crawlab_create_project",
  update_project: "crawlab_update_project",
  delete_project: "crawlab_delete_project",
};

export function configureProjectTools(server: McpServer, client: CrawlabClient) {
  server.tool(
    PROJECT_TOOLS.list_projects,
    "List all projects in Crawlab",
    {
      page: z.number().optional().describe("Page number for pagination (default: 1)"),
      size: z.number().optional().describe("Number of projects per page (default: 10)"),
    },
    async ({ page, size }) => {
      try {
        const response = await client.getProjects({ page, size });
        return {
          content: [
            {
              type: "text",
              text: JSON.stringify(response, null, 2),
            },
          ],
        };
      } catch (error) {
        return {
          content: [
            {
              type: "text",
              text: `Error listing projects: ${error instanceof Error ? error.message : String(error)}`,
            },
          ],
          isError: true,
        };
      }
    }
  );

  server.tool(
    PROJECT_TOOLS.get_project,
    "Get details of a specific project",
    {
      project_id: z.string().describe("The ID of the project to retrieve"),
    },
    async ({ project_id }) => {
      try {
        const response = await client.getProject(project_id);
        return {
          content: [
            {
              type: "text",
              text: JSON.stringify(response, null, 2),
            },
          ],
        };
      } catch (error) {
        return {
          content: [
            {
              type: "text",
              text: `Error getting project: ${error instanceof Error ? error.message : String(error)}`,
            },
          ],
          isError: true,
        };
      }
    }
  );

  server.tool(
    PROJECT_TOOLS.create_project,
    "Create a new project",
    {
      name: z.string().describe("Name of the project"),
      description: z.string().optional().describe("Description of the project"),
    },
    async ({ name, description }) => {
      try {
        const projectData = {
          name,
          description,
        };
        const response = await client.createProject(projectData);
        return {
          content: [
            {
              type: "text",
              text: JSON.stringify(response, null, 2),
            },
          ],
        };
      } catch (error) {
        return {
          content: [
            {
              type: "text",
              text: `Error creating project: ${error instanceof Error ? error.message : String(error)}`,
            },
          ],
          isError: true,
        };
      }
    }
  );

  server.tool(
    PROJECT_TOOLS.update_project,
    "Update an existing project",
    {
      project_id: z.string().describe("The ID of the project to update"),
      name: z.string().optional().describe("New name for the project"),
      description: z.string().optional().describe("New description for the project"),
    },
    async ({ project_id, name, description }) => {
      try {
        const updateData = {
          ...(name && { name }),
          ...(description && { description }),
        };
        const response = await client.updateProject(project_id, updateData);
        return {
          content: [
            {
              type: "text",
              text: JSON.stringify(response, null, 2),
            },
          ],
        };
      } catch (error) {
        return {
          content: [
            {
              type: "text",
              text: `Error updating project: ${error instanceof Error ? error.message : String(error)}`,
            },
          ],
          isError: true,
        };
      }
    }
  );

  server.tool(
    PROJECT_TOOLS.delete_project,
    "Delete a project",
    {
      project_id: z.string().describe("The ID of the project to delete"),
    },
    async ({ project_id }) => {
      try {
        await client.deleteProject(project_id);
        return {
          content: [
            {
              type: "text",
              text: `Project ${project_id} deleted successfully.`,
            },
          ],
        };
      } catch (error) {
        return {
          content: [
            {
              type: "text",
              text: `Error deleting project: ${error instanceof Error ? error.message : String(error)}`,
            },
          ],
          isError: true,
        };
      }
    }
  );
}

export { PROJECT_TOOLS };
