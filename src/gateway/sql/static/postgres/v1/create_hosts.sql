CREATE TABLE IF NOT EXISTS "hosts" (
  "id" SERIAL PRIMARY KEY,
  "api_id" INTEGER NOT NULL,
  "name" TEXT NOT NULL,
  FOREIGN KEY("api_id") REFERENCES "apis"("id") ON DELETE CASCADE
);