#pragma once

#ifdef __cplusplus
#include "duckdb.hpp"

namespace duckdb {
  class GoFSExtension : public Extension {
  public:
    void Load(ExtensionLoader &loader) override;
    std::string Name() override;
    std::string Version() const override;
  };
}

extern "C" {
#else
#include "duckdb.h"
#endif // __cplusplus

duckdb_state duckfs_register_subsystem(duckdb_database database, int id);
duckdb_state duckfs_unregister_subsystem(duckdb_database database);

// DuckDB file flags (from duckdb/common/file_open_flags.hpp)
#define DUCKDB_FILE_FLAGS_READ (1 << 0)
#define DUCKDB_FILE_FLAGS_WRITE (1 << 1)
#define DUCKDB_FILE_FLAGS_FILE_CREATE (1 << 3)
#define DUCKDB_FILE_FLAGS_FILE_CREATE_NEW (1 << 4)
#define DUCKDB_FILE_FLAGS_APPEND (1 << 5)
#define DUCKDB_FILE_FLAGS_NULL_IF_NOT_EXISTS (1 << 7)
#define DUCKDB_FILE_FLAGS_EXCLUSIVE_CREATE (1 << 9)
#define DUCKDB_FILE_FLAGS_NULL_IF_EXISTS (1 << 10)

#ifdef __cplusplus
}
#endif // __cplusplus
