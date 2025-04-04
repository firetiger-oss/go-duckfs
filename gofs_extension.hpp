#pragma once

#ifdef __cplusplus
#include <duckdb.hpp>

namespace duckdb {

  class GoFSExtension : public Extension {
  public:
    void Load(DuckDB &db) override;
    std::string Name() override;
    std::string Version() const override;
  };

}

extern "C" {
#endif // __cplusplus
#include <duckdb.h>

duckdb_state duckfs_register_subsystem(duckdb_database database, int id);
duckdb_state duckfs_unregister_subsystem(duckdb_database database);

#ifdef __cplusplus
}
#endif // __cplusplus
