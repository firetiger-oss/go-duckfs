#define DUCKDB_EXTENSION_MAIN

#include <duckdb.hpp>
#include <duckdb/common/exception.hpp>
#include <duckdb/common/string_util.hpp>
#include <duckdb/main/extension_util.hpp>
#include <duckdb/main/capi/capi_internal.hpp>
#include <gofs_extension.hpp>

extern "C" {
  int duckfs_file_exists(int id, const char *path);

  int duckfs_file_open(int id, const char *path);

  int duckfs_file_close(int id);

  int64_t duckfs_file_size(int id);

  int64_t duckfs_file_read_at(int id, void *buf, int64_t size, int64_t off);

  int64_t duckfs_file_read(int id, void *buf, int64_t size);

  int64_t duckfs_file_seek(int id, int64_t off, int whence);
}

namespace duckdb {
  constexpr const char *GOFS_FILESYSTEM_NAME = "GoFileSystem";
  constexpr int GOFS_SEEK_SET = 0;
  constexpr int GOFS_SEEK_CUR = 1;
  constexpr int GOFS_SEEK_END = 2;

  class GoFileHandle : public FileHandle {
    friend class GoFileSystem;
  public:
    GoFileHandle(FileSystem &fs, string path, FileOpenFlags flags, int id) :
      FileHandle(fs, path, flags),
      id(id) {
    }

    ~GoFileHandle() override {
      this->Close();
    }

    void Close() override {
      if (this->id >= 0) {
	duckfs_file_close(this->id);
	this->id = -1;
      }
    }

  private:
    int id;
  };

  class GoFileSystem : public FileSystem {
  public:
    GoFileSystem(int id) :
      FileSystem(),
      id(id) {
    }

    string GetName() const override {
      return GOFS_FILESYSTEM_NAME;
    }

    bool CanHandleFile(const string &) override {
      return true;
    }

    bool CanSeek() override {
      return true;
    }

    bool OnDiskFile(FileHandle &handle) override {
      return false;
    }

    bool FileExists(const string &filename, optional_ptr<FileOpener> opener) override {
      return duckfs_file_exists(this->id, filename.c_str());
    }

    vector<string> Glob(const string &path, FileOpener *opener) override {
      return {path}; // FIXME
    }

    unique_ptr<FileHandle> OpenFile(const string &path, FileOpenFlags flags, optional_ptr<FileOpener> opener) override {
      auto id = duckfs_file_open(this->id, path.c_str());
      if (id < 0) {
	// This appears to be the right way to report errors opening files,
	// usually indicating that the file does not exist. In several places,
	// it causes DuckDB to throw an exception indicating that a null pointer
	// was being dereferenced (e.g., when reading parquet files). These
	// errors are handled propertly in the C and Go bindings, and reported
	// to the callers as Go errors.
	return nullptr;
      }
      return make_uniq<GoFileHandle>(*this, path, flags, id);
    }

    int64_t GetFileSize(FileHandle &handle) override {
      auto f = dynamic_cast<GoFileHandle*>(&handle);
      auto size = duckfs_file_size(f->id);
      if (size < 0) {
	throw IOException("duckdb failed to get file size: "  + handle.GetPath());
      }
      return size;
    }

    void Read(FileHandle &handle, void *buf, int64_t size, idx_t off) override {
      auto f = dynamic_cast<GoFileHandle*>(&handle);
      auto n = duckfs_file_read_at(f->id, buf, size, off);
      if (n < 0) {
	throw IOException("duckdb failed to read file at location: " + handle.GetPath());
      }
      if (n < size) {
	throw IOException("duckdb read less than requested bytes: " + handle.GetPath());
      }
    }

    int64_t Read(FileHandle &handle, void *buf, int64_t size) override {
      auto f = dynamic_cast<GoFileHandle*>(&handle);
      auto n = duckfs_file_read(f->id, buf, size);
      if (n < 0) {
	throw IOException("duckdb failed to read file: " + handle.GetPath());
      }
      return n;
    }

    void Seek(FileHandle &handle, idx_t off) override {
      auto f = dynamic_cast<GoFileHandle*>(&handle);
      auto n = duckfs_file_seek(f->id, off, GOFS_SEEK_SET);
      if (n < 0) {
	throw IOException("duckdb failed to seek to new file position: " + handle.GetPath());
      }
    }

    void Reset(FileHandle &handle) override {
      auto f = dynamic_cast<GoFileHandle*>(&handle);
      auto n = duckfs_file_seek(f->id, 0, GOFS_SEEK_SET);
      if (n < 0) {
	throw IOException("duckdb failed to reset file seek position: " + handle.GetPath());
      }
    }

    idx_t SeekPosition(FileHandle &handle) override {
      auto f = dynamic_cast<GoFileHandle*>(&handle);
      auto n = duckfs_file_seek(f->id, 0, GOFS_SEEK_CUR);
      if (n < 0) {
	throw IOException("duckdb failed get current file seek position: " + handle.GetPath());
      }
      return n;
    }
    
  private:
    int id;
  };

  static void LoadInternal(DatabaseInstance &instance) {
  }

  void GoFSExtension::Load(DuckDB &db) {
    LoadInternal(*db.instance);
  }

  string GoFSExtension::Name() {
    return "gofs";
  }

  string GoFSExtension::Version() const {
    return "";
  }
}

extern "C" {
  DUCKDB_EXTENSION_API void gofs_init(duckdb::DatabaseInstance &db) {
    duckdb::DuckDB wrapper(db);
    wrapper.LoadExtension<duckdb::GoFSExtension>();
  }

  DUCKDB_EXTENSION_API const char *gofs_version() {
    return duckdb::DuckDB::LibraryVersion();
  }

  duckdb_state duckfs_register_subsystem(duckdb_database database, int id) {
    if (!database || id < 0) {
      return DuckDBError;
    }
    auto wrapper = reinterpret_cast<duckdb::DatabaseWrapper *>(database);
    try {
      auto &fs = duckdb::FileSystem::GetFileSystem(*wrapper->database->instance.get());
      fs.RegisterSubSystem(duckdb::make_uniq<duckdb::GoFileSystem>(id));
    } catch (...) {
      return DuckDBError;
    }
    return DuckDBSuccess;
  }

  duckdb_state duckfs_unregister_subsystem(duckdb_database database) {
    if (!database) {
      return DuckDBError;
    }
    auto wrapper = reinterpret_cast<duckdb::DatabaseWrapper *>(database);
    try {
      auto &fs = duckdb::FileSystem::GetFileSystem(*wrapper->database->instance.get());
      fs.UnregisterSubSystem(duckdb::GOFS_FILESYSTEM_NAME);
    } catch (...) {
      return DuckDBError;
    }
    return DuckDBSuccess;
  }
}

#ifndef DUCKDB_EXTENSION_MAIN
#error DUCKDB_EXTENSION_MAIN not defined
#endif
